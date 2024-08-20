package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/etix/goscrape"
	_ "github.com/mattn/go-sqlite3"
)

type Torrent struct {
	URL                 string `json:"url"`
	TopLevelGroupName   string `json:"top_level_group_name"`
	GroupName           string `json:"group_name"`
	DisplayName         string `json:"display_name"`
	AddedToTorrentsList string `json:"added_to_torrents_list_at"`
	IsMetadata          bool   `json:"is_metadata"`
	BTIH                string `json:"btih"`
	MagnetLink          string `json:"magnet_link"`
	TorrentSize         int    `json:"torrent_size"`
	NumFiles            int    `json:"num_files"`
	DataSize            int64  `json:"data_size"`
	AACurrentlySeeding  bool   `json:"aa_currently_seeding"`
	Obsolete            bool   `json:"obsolete"`
	Embargo             bool   `json:"embargo"`
	Seeders             int    `json:"seeders"`
	Leechers            int    `json:"leechers"`
	Completed           int    `json:"completed"`
	StatsScrapedAt      string `json:"stats_scraped_at"`
	PartiallyBroken     bool   `json:"partially_broken"`
	FormattedSeeders    string
	FormattedLeechers   string
	SeedersColor        string
	LeechersColor       string
	FormattedDataSize   string
	FormattedAddedDate  string
	MetadataLabel       string
	StatusLabel         template.HTML
	SeederStats         []SeederStat `json:"seederStats"`
}

type GroupData struct {
	TopLevelGroupName string
	GroupName         string
	Torrents          []Torrent
	TotalCount        int
}

func wrap(handler func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("Panic in %s: %v\n%s", r.URL.Path, rec, debug.Stack())
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()

		err := handler(w, r)
		if err != nil {
			log.Printf("Error in %s: %v", r.URL.Path, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}
}

var readDB, writeDB *sql.DB

func initDB(dbPath string) error {
	var err error
	readDB, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("error opening read database: %v", err)
	}
	writeDB, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("error opening write database: %v", err)
	}

	// Enable WAL mode for both connections
	for _, db := range []*sql.DB{readDB, writeDB} {
		_, err = db.Exec("PRAGMA journal_mode=WAL;")
		if err != nil {
			return fmt.Errorf("error setting journal mode: %v", err)
		}
	}

	return nil
}

func withReadTx(fn func(*sql.Tx) error) error {
	tx, err := readDB.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("error beginning read transaction: %v", err)
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}

var (
	torrentsEnabled bool
	dbPath          string
	assetsPath      string
	torrentsPath    string
)

func main() {
	port := flag.Int("port", 8080, "Port to run the server on")
	dataDir := flag.String("directory", ".", "Directory to store data")
	flag.BoolVar(&torrentsEnabled, "torrents", false, "Download and store torrent files")
	flag.Parse()

	// Define paths
	dbPath = filepath.Join(*dataDir, "torrents.db")
	assetsPath = filepath.Join(*dataDir, "assets")
	torrentsPath = filepath.Join(assetsPath, "torrents")

	// Ensure directories exist
	if err := os.MkdirAll(assetsPath, 0755); err != nil {
		log.Fatalf("Error creating assets directory: %v", err)
	}
	if err := os.MkdirAll(torrentsPath, 0755); err != nil {
		log.Fatalf("Error creating torrents directory: %v", err)
	}

	// Ensure necessary directories exist
	if err := os.MkdirAll(assetsPath, 0755); err != nil {
		log.Fatalf("Error creating assets directory: %v", err)
	}
	if torrentsEnabled {
		if err := os.MkdirAll(torrentsPath, 0755); err != nil {
			log.Fatalf("Error creating torrents directory: %v", err)
		}
	}

	err := initDB(dbPath)
	if err != nil {
		log.Fatalf("Error initializing database: %v", err)
	}
	defer readDB.Close()
	defer writeDB.Close()

	createTable()

	go updateTorrents()
	go updateStats()
	go updateDailySeederStats()

	http.HandleFunc("/", wrap(handleRoot))
	http.HandleFunc("/full/", wrap(handleFullList))
	http.HandleFunc("/stats/", wrap(handleStats))
	http.HandleFunc("/seeder-stats/", wrap(handleSeederStats))
	http.HandleFunc("/json", wrap(handleTorrentStats))
	http.HandleFunc("/generate-torrent-list", wrap(handleGenerateTorrentList))
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(assetsPath))))

	log.Printf("Starting server on port %d", *port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}

func createTable() {
	_, err := writeDB.Exec(`
		CREATE TABLE IF NOT EXISTS torrents (
			btih TEXT PRIMARY KEY,
			url TEXT,
			top_level_group_name TEXT,
			group_name TEXT,
			display_name TEXT,
			added_to_torrents_list DATETIME,
			is_metadata INTEGER,
			magnet_link TEXT,
			torrent_size INTEGER,
			num_files INTEGER,
			data_size INTEGER,
			aa_currently_seeding INTEGER,
			obsolete INTEGER,
			embargo INTEGER,
			seeders INTEGER,
			leechers INTEGER,
			completed INTEGER,
			stats_scraped_at DATETIME,
			partially_broken INTEGER
		)
	`)
	if err != nil {
		log.Fatalf("Error creating table: %v", err)
	}
	_, err = writeDB.Exec(`
	CREATE TABLE IF NOT EXISTS torrent_updates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		btih TEXT,
		seeders INTEGER,
		leechers INTEGER,
		completed INTEGER,
		stats_scraped_at DATETIME,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)
`)
	if err != nil {
		log.Fatalf("Error creating torrent_updates table: %v", err)
	}
	_, err = writeDB.Exec(`
	CREATE TABLE IF NOT EXISTS daily_seeder_stats (
    date DATE PRIMARY KEY,
    low_seeders_tb FLOAT,  -- Total TB for torrents with <4 seeders
    medium_seeders_tb FLOAT,  -- Total TB for torrents with 4-10 seeders
    high_seeders_tb FLOAT  -- Total TB for torrents with >10 seeders
);
`)
	if err != nil {
		log.Fatalf("Error creating torrent_updates table: %v", err)
	}
}

func updateTorrents() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		if err := performTorrentUpdate(); err != nil {
			log.Printf("Error updating torrents: %v", err)
		}
		<-ticker.C
	}
}

func performTorrentUpdate() error {
	resp, err := http.Get("https://annas-archive.org/dyn/torrents.json")
	if err != nil {
		return fmt.Errorf("error fetching torrents: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %w", err)
	}

	var torrentsData []Torrent
	if err := json.Unmarshal(body, &torrentsData); err != nil {
		return fmt.Errorf("error decoding torrents: %w", err)
	}

	// Ensure the assets directory exists
	if err := os.MkdirAll(assetsPath, 0755); err != nil {
		return fmt.Errorf("error creating assets directory: %w", err)
	}

	if torrentsEnabled {
		// Ensure the torrents directory exists
		if err := os.MkdirAll(torrentsPath, 0755); err != nil {
			return fmt.Errorf("error creating torrents directory: %w", err)
		}
	}

	for _, t := range torrentsData {
		_, err := writeDB.Exec(`
            INSERT OR REPLACE INTO torrents (
                btih, url, top_level_group_name, group_name, display_name, added_to_torrents_list,
                is_metadata, magnet_link, torrent_size, num_files, data_size, aa_currently_seeding,
                obsolete, embargo, seeders, leechers, completed, stats_scraped_at, partially_broken
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.BTIH, t.URL, t.TopLevelGroupName, t.GroupName, t.DisplayName, t.AddedToTorrentsList,
			t.IsMetadata, t.MagnetLink, t.TorrentSize, t.NumFiles, t.DataSize, t.AACurrentlySeeding,
			t.Obsolete, t.Embargo, t.Seeders, t.Leechers, t.Completed, t.StatsScrapedAt, t.PartiallyBroken)

		if err != nil {
			log.Printf("Error inserting/updating torrent %s: %v", t.BTIH, err)
		} else {
			log.Printf("Successfully inserted/updated torrent: %s", t.BTIH)
		}

		// Download torrent file if --torrents flag is set
		if torrentsEnabled {
			torrentPath := filepath.Join(torrentsPath, t.BTIH+".torrent")
			if _, err := os.Stat(torrentPath); os.IsNotExist(err) {
				if err := downloadTorrentFile(t.URL, torrentPath); err != nil {
					log.Printf("Error downloading torrent file for %s: %v", t.BTIH, err)
				} else {
					log.Printf("Successfully downloaded torrent file: %s", t.BTIH)
				}
			}
		}
	}

	log.Println("Torrents updated successfully")
	return nil
}

func downloadTorrentFile(url, filepath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("error downloading torrent file: %w", err)
	}
	defer resp.Body.Close()

	out, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("error creating torrent file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("error writing torrent file: %w", err)
	}

	return nil
}

func updateStats() {
	log.Println("Starting stats scraping process...")

	ticker := time.NewTicker(30 * time.Minute)
	for {
		<-ticker.C

		var allInfohashes []string
		err := withReadTx(func(tx *sql.Tx) error {
			// Query to select all btih values
			rows, err := tx.Query("SELECT btih FROM torrents")
			if err != nil {
				return fmt.Errorf("error querying torrents: %v", err)
			}
			defer rows.Close()

			// Slice to hold the infohash values
			for rows.Next() {
				var btih string
				if err := rows.Scan(&btih); err != nil {
					return fmt.Errorf("error scanning row: %v", err)
				}
				allInfohashes = append(allInfohashes, btih)
			}

			// Check for any errors encountered during iteration
			if err := rows.Err(); err != nil {
				return fmt.Errorf("error iterating rows: %v", err)
			}

			return nil
		})

		if err != nil {
			log.Printf("Error fetching infohashes: %v", err)
			continue
		}

		// Create a new instance of the goscrape library
		s, err := goscrape.New("udp://tracker.opentrackr.org:1337/announce")
		if err != nil {
			log.Printf("Error creating goscrape instance: %v", err)
			continue
		}

		// Process infohashes in bundles of 50
		const bundleSize = 50
		for i := 0; i < len(allInfohashes); i += bundleSize {
			end := i + bundleSize
			if end > len(allInfohashes) {
				end = len(allInfohashes)
			}
			bundle := allInfohashes[i:end]

			// Convert bundle to [][]byte
			infohash := make([][]byte, len(bundle))
			for j, hash := range bundle {
				infohash[j] = []byte(hash)
			}

			// Scrape the current bundle
			res, err := s.Scrape(infohash...)
			if err != nil {
				log.Printf("Error scraping infohashes: %v", err)
				continue
			}

			// Loop over the results and update the database
			for _, r := range res {
				fmt.Printf("Infohash: %s, Seeders: %d, Leechers: %d, Completed: %d\n",
					string(r.Infohash), r.Seeders, r.Leechers, r.Completed)

				// Use a transaction for the updates
				tx, err := writeDB.Begin()
				if err != nil {
					log.Printf("Error beginning transaction: %v", err)
					continue
				}

				// Update torrents table
				_, err = tx.Exec(`
                    UPDATE torrents
                    SET seeders = ?, leechers = ?, completed = ?, stats_scraped_at = ?
                    WHERE btih = ?
                `, r.Seeders, r.Leechers, r.Completed, time.Now(), string(r.Infohash))
				if err != nil {
					tx.Rollback()
					log.Printf("Error updating torrent: %v", err)
					continue
				}

				// Insert update details into torrent_updates table
				_, err = tx.Exec(`
                    INSERT INTO torrent_updates (
                        btih, seeders, leechers, completed, stats_scraped_at
                    ) VALUES (?, ?, ?, ?, ?)
                `, string(r.Infohash), r.Seeders, r.Leechers, r.Completed, time.Now())
				if err != nil {
					tx.Rollback()
					log.Printf("Error inserting update into torrent_updates: %v", err)
					continue
				}

				if err := tx.Commit(); err != nil {
					log.Printf("Error committing transaction: %v", err)
				}
			}
		}

		log.Println("Stats updated successfully")
	}
}

func handleSeederStats(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return fmt.Errorf("method not allowed: %s", r.Method)
	}

	var stats []struct {
		Date            string  `json:"date"`
		LowSeedersTB    float64 `json:"lowSeedersTB"`
		MediumSeedersTB float64 `json:"mediumSeedersTB"`
		HighSeedersTB   float64 `json:"highSeedersTB"`
	}

	err := withReadTx(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT date, low_seeders_tb, medium_seeders_tb, high_seeders_tb FROM daily_seeder_stats ORDER BY date")
		if err != nil {
			return fmt.Errorf("error querying database: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var stat struct {
				Date            time.Time `json:"-"`
				LowSeedersTB    float64   `json:"lowSeedersTB"`
				MediumSeedersTB float64   `json:"mediumSeedersTB"`
				HighSeedersTB   float64   `json:"highSeedersTB"`
			}
			if err := rows.Scan(&stat.Date, &stat.LowSeedersTB, &stat.MediumSeedersTB, &stat.HighSeedersTB); err != nil {
				return fmt.Errorf("error scanning row: %w", err)
			}

			formattedStat := struct {
				Date            string  `json:"date"`
				LowSeedersTB    float64 `json:"lowSeedersTB"`
				MediumSeedersTB float64 `json:"mediumSeedersTB"`
				HighSeedersTB   float64 `json:"highSeedersTB"`
			}{
				Date:            stat.Date.Format("2006-01-02"), // Format date as YYYY-MM-DD
				LowSeedersTB:    stat.LowSeedersTB,
				MediumSeedersTB: stat.MediumSeedersTB,
				HighSeedersTB:   stat.HighSeedersTB,
			}

			stats = append(stats, formattedStat)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("error after iterating rows: %w", err)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("error fetching seeder stats: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(stats)
}

func updateDailySeederStats() {
	log.Println("Starting daily seeder stats update process...")

	performUpdate := func() error {
		var lowSeedersTB, mediumSeedersTB, highSeedersTB float64

		// Use readDB for the query
		err := withReadTx(func(tx *sql.Tx) error {
			rows, err := tx.Query(`
                SELECT 
                    SUM(CASE WHEN seeders < 4 THEN data_size ELSE 0 END) / 1099511627776.0 AS low_seeders_tb,
                    SUM(CASE WHEN seeders BETWEEN 4 AND 10 THEN data_size ELSE 0 END) / 1099511627776.0 AS medium_seeders_tb,
                    SUM(CASE WHEN seeders > 10 THEN data_size ELSE 0 END) / 1099511627776.0 AS high_seeders_tb
                FROM torrents 
                WHERE embargo = 0
            `)
			if err != nil {
				return fmt.Errorf("error querying seeder stats: %w", err)
			}
			defer rows.Close()

			if rows.Next() {
				err = rows.Scan(&lowSeedersTB, &mediumSeedersTB, &highSeedersTB)
				if err != nil {
					return fmt.Errorf("error scanning row: %w", err)
				}
			}

			return rows.Err()
		})

		if err != nil {
			return fmt.Errorf("error reading seeder stats: %w", err)
		}

		// Use writeDB for the update
		tx, err := writeDB.Begin()
		if err != nil {
			return fmt.Errorf("error starting write transaction: %w", err)
		}
		defer tx.Rollback()

		// Insert or update the daily stats
		_, err = tx.Exec(`
            INSERT OR REPLACE INTO daily_seeder_stats (date, low_seeders_tb, medium_seeders_tb, high_seeders_tb)
            VALUES (DATE('now'), ?, ?, ?)`,
			lowSeedersTB, mediumSeedersTB, highSeedersTB)

		if err != nil {
			return fmt.Errorf("error inserting/updating daily seeder stats: %w", err)
		}

		// Commit the transaction
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("error committing transaction: %w", err)
		}

		log.Println("Daily seeder stats updated successfully")
		return nil
	}

	// Perform the first update immediately
	if err := performUpdate(); err != nil {
		log.Printf("Error in initial performUpdate: %v", err)
	}

	// Set up a ticker to run once a day
	ticker := time.NewTicker(24 * time.Hour)
	for range ticker.C {
		if err := performUpdate(); err != nil {
			log.Printf("Error in performUpdate: %v", err)
		}
	}
}

func handleTorrentStats(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return fmt.Errorf("method not allowed: %s", r.Method)
	}

	var torrents []Torrent

	err := withReadTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`
            SELECT 
                url, 
                top_level_group_name, 
                group_name, 
                display_name, 
                added_to_torrents_list, 
                is_metadata, 
                btih, 
                magnet_link, 
                torrent_size, 
                num_files, 
                data_size, 
                aa_currently_seeding, 
                obsolete, 
                embargo, 
                seeders, 
                leechers, 
                completed, 
                stats_scraped_at, 
                partially_broken 
            FROM torrents
        `)
		if err != nil {
			return fmt.Errorf("error querying database: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var t Torrent
			if err := rows.Scan(
				&t.URL,
				&t.TopLevelGroupName,
				&t.GroupName,
				&t.DisplayName,
				&t.AddedToTorrentsList,
				&t.IsMetadata,
				&t.BTIH,
				&t.MagnetLink,
				&t.TorrentSize,
				&t.NumFiles,
				&t.DataSize,
				&t.AACurrentlySeeding,
				&t.Obsolete,
				&t.Embargo,
				&t.Seeders,
				&t.Leechers,
				&t.Completed,
				&t.StatsScrapedAt,
				&t.PartiallyBroken,
			); err != nil {
				return fmt.Errorf("error scanning row: %w", err)
			}
			torrents = append(torrents, t)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("error after iterating rows: %w", err)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("error fetching torrent stats: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(torrents)
}

func handleRoot(w http.ResponseWriter, r *http.Request) error {
	torrents, err := fetchTorrents("ORDER BY top_level_group_name, group_name, seeders ASC")
	if err != nil {
		return fmt.Errorf("error fetching torrents: %w", err)
	}

	groups := groupTorrents(torrents)
	return renderTemplate(w, "root", groups)
}

func handleFullList(w http.ResponseWriter, r *http.Request) error {
	groupName := strings.TrimPrefix(r.URL.Path, "/full/")
	torrents, err := fetchTorrents("WHERE group_name = ? ORDER BY seeders ASC", groupName)
	if err != nil {
		return fmt.Errorf("error fetching torrents: %w", err)
	}

	data := struct {
		GroupName string
		Torrents  []Torrent
	}{
		GroupName: groupName,
		Torrents:  torrents,
	}
	return renderTemplate(w, "fullList", data)
}

func handleStats(w http.ResponseWriter, r *http.Request) error {
	btih := r.URL.Path[len("/stats/"):]
	torrents, err := fetchTorrents("WHERE btih = ?", btih)
	if err != nil {
		return fmt.Errorf("error fetching torrent stats: %w", err)
	}

	if len(torrents) == 0 {
		http.NotFound(w, r)
		return nil
	}

	// Fetch daily average seeder counts
	seederStats, err := fetchDailySeederStats(btih)
	if err != nil {
		return fmt.Errorf("error fetching seeder stats: %w", err)
	}

	// Add SeederStats to the Torrent struct
	torrents[0].SeederStats = seederStats

	return renderTemplate(w, "stats", torrents[0])
}

type SeederStat struct {
	Date    string  `json:"date"`
	Seeders float64 `json:"seeders"`
}

func fetchDailySeederStats(btih string) ([]SeederStat, error) {
	query := `
        SELECT 
            DATE(stats_scraped_at) as date,
            AVG(seeders) as avg_seeders
        FROM 
            torrent_updates
        WHERE 
            btih = ?
        GROUP BY 
            DATE(stats_scraped_at)
        ORDER BY 
            date ASC
    `

	var stats []SeederStat

	err := withReadTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(query, btih)
		if err != nil {
			return fmt.Errorf("error querying database: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var stat SeederStat
			err := rows.Scan(&stat.Date, &stat.Seeders)
			if err != nil {
				return fmt.Errorf("error scanning row: %w", err)
			}
			stats = append(stats, stat)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("error after iterating rows: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error fetching daily seeder stats: %w", err)
	}

	return stats, nil
}

func handleGenerateTorrentList(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("method not allowed: %s", r.Method)
	}

	maxTB, err := strconv.ParseFloat(r.FormValue("maxTB"), 64)
	if err != nil {
		return fmt.Errorf("invalid max TB value: %w", err)
	}

	listType := r.FormValue("listType")
	format := r.FormValue("format")

	torrents, err := generateTorrentList(maxTB, listType)
	if err != nil {
		return fmt.Errorf("error generating torrent list: %w", err)
	}

	switch format {
	case "xml":
		w.Header().Set("Content-Type", "application/xml")
		xmlResponse, err := convertToXML(torrents)
		if err != nil {
			return fmt.Errorf("error converting to XML: %w", err)
		}
		_, err = w.Write(xmlResponse)
		return err

	case "txt":
		w.Header().Set("Content-Type", "text/plain")
		txtResponse := convertToTXT(torrents)
		_, err = w.Write([]byte(txtResponse))
		return err

	case "json":
		fallthrough
	default:
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(torrents)
	}
}

func fetchTorrents(query string, args ...interface{}) ([]Torrent, error) {
	var torrents []Torrent

	err := withReadTx(func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT * FROM torrents "+query, args...)
		if err != nil {
			return fmt.Errorf("error querying torrents: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var t Torrent
			var addedDateStr, statsScrapedAtStr sql.NullString
			err := rows.Scan(
				&t.BTIH, &t.URL, &t.TopLevelGroupName, &t.GroupName, &t.DisplayName,
				&addedDateStr, &t.IsMetadata, &t.MagnetLink, &t.TorrentSize, &t.NumFiles,
				&t.DataSize, &t.AACurrentlySeeding, &t.Obsolete, &t.Embargo, &t.Seeders, &t.Leechers,
				&t.Completed, &statsScrapedAtStr, &t.PartiallyBroken)
			if err != nil {
				return fmt.Errorf("error scanning torrent: %w", err)
			}

			// Handle NULL values
			if addedDateStr.Valid {
				t.AddedToTorrentsList = addedDateStr.String
			} else {
				t.AddedToTorrentsList = ""
			}

			if statsScrapedAtStr.Valid {
				t.StatsScrapedAt = statsScrapedAtStr.String
			} else {
				t.StatsScrapedAt = ""
			}

			formatTorrent(&t)
			torrents = append(torrents, t)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("error iterating rows: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error fetching torrents: %w", err)
	}

	return torrents, nil
}

func formatTorrent(t *Torrent) {
	t.FormattedSeeders = formatNumber(t.Seeders)
	t.FormattedLeechers = formatNumber(t.Leechers)
	t.FormattedDataSize = formatDataSize(t.DataSize)
	t.FormattedAddedDate = formatDate(t.AddedToTorrentsList)
	t.MetadataLabel = "data"
	if t.IsMetadata {
		t.MetadataLabel = "metadata"
	}
	t.StatusLabel = formatStatus(t.AACurrentlySeeding, t.Seeders, t.Leechers, t.StatsScrapedAt)
}

func groupTorrents(torrents []Torrent) []GroupData {
	groups := make(map[string]map[string][]Torrent)
	for _, t := range torrents {
		if groups[t.TopLevelGroupName] == nil {
			groups[t.TopLevelGroupName] = make(map[string][]Torrent)
		}
		groups[t.TopLevelGroupName][t.GroupName] = append(groups[t.TopLevelGroupName][t.GroupName], t)
	}

	var groupData []GroupData
	for topLevelGroup, groupMap := range groups {
		for group, torrents := range groupMap {
			limitedTorrents := torrents
			if len(torrents) > 20 {
				limitedTorrents = torrents[:20]
			}
			groupData = append(groupData, GroupData{
				TopLevelGroupName: topLevelGroup,
				GroupName:         group,
				Torrents:          limitedTorrents,
				TotalCount:        len(torrents),
			})
		}
	}
	return groupData
}

func renderTemplate(w http.ResponseWriter, name string, data interface{}) error {
	funcMap := template.FuncMap{
		"urlsafe": func(s string) template.URL { return template.URL(s) },
		"json": func(v interface{}) template.JS {
			b, err := json.Marshal(v)
			if err != nil {
				return template.JS("null")
			}
			return template.JS(b)
		},
		"torrentsEnabled": func() bool { return torrentsEnabled },
	}

	tmpl := template.Must(template.New(name).Funcs(funcMap).Parse(getTemplateContent(name)))
	return tmpl.Execute(w, data)
}

func getTemplateContent(name string) string {
	header := `
<!DOCTYPE html>
<html>
<head>
    <title>Anna's Archive Mirror</title>
	<link rel="icon" type="image/x-icon" href="/assets/favicon.ico">
    <style>
	.column,.columns,.container{width:100%;box-sizing:border-box}h1,h2,h3{letter-spacing:-.1rem}body,h6{line-height:1.6}ol,p,ul{margin-top:0}.container{position:relative;max-width:960px;margin:0 auto;padding:0 20px}.column,.columns{float:left}@media (min-width:400px){.container{width:85%;padding:0}}html{font-size:62.5%}body{font-size:1.5em;font-weight:400;font-family:Raleway,HelveticaNeue,"Helvetica Neue",Helvetica,Arial,sans-serif;color:#222}h1,h2,h3,h4,h5,h6{margin-top:0;margin-bottom:2rem;font-weight:300}h1{font-size:4rem;line-height:1.2}h2{font-size:3.6rem;line-height:1.25}h3{font-size:3rem;line-height:1.3}h4{font-size:2.4rem;line-height:1.35;letter-spacing:-.08rem}h5{font-size:1.8rem;line-height:1.5;letter-spacing:-.05rem}h6{font-size:1.5rem;letter-spacing:0}@media (min-width:550px){.container{width:80%}.column,.columns{margin-left:4%}.column:first-child,.columns:first-child{margin-left:0}.one.column,.one.columns{width:4.66666666667%}.two.columns{width:13.3333333333%}.three.columns{width:22%}.four.columns,.one-third.column{width:30.6666666667%}.five.columns{width:39.3333333333%}.one-half.column,.six.columns{width:48%}.seven.columns{width:56.6666666667%}.eight.columns,.two-thirds.column{width:65.3333333333%}.nine.columns{width:74%}.ten.columns{width:82.6666666667%}.eleven.columns{width:91.3333333333%}.twelve.columns{width:100%;margin-left:0}.offset-by-one.column,.offset-by-one.columns{margin-left:8.66666666667%}.offset-by-two.column,.offset-by-two.columns{margin-left:17.3333333333%}.offset-by-three.column,.offset-by-three.columns{margin-left:26%}.offset-by-four.column,.offset-by-four.columns,.offset-by-one-third.column,.offset-by-one-third.columns{margin-left:34.6666666667%}.offset-by-five.column,.offset-by-five.columns{margin-left:43.3333333333%}.offset-by-one-half.column,.offset-by-one-half.columns,.offset-by-six.column,.offset-by-six.columns{margin-left:52%}.offset-by-seven.column,.offset-by-seven.columns{margin-left:60.6666666667%}.offset-by-eight.column,.offset-by-eight.columns,.offset-by-two-thirds.column,.offset-by-two-thirds.columns{margin-left:69.3333333333%}.offset-by-nine.column,.offset-by-nine.columns{margin-left:78%}.offset-by-ten.column,.offset-by-ten.columns{margin-left:86.6666666667%}.offset-by-eleven.column,.offset-by-eleven.columns{margin-left:95.3333333333%}h1{font-size:5rem}h2{font-size:4.2rem}h3{font-size:3.6rem}h4{font-size:3rem}h5{font-size:2.4rem}h6{font-size:1.5rem}}a{color:#1eaedb}a:hover{color:#0fa0ce}.button,button,input[type=button],input[type=reset],input[type=submit]{display:inline-block;height:38px;padding:0 30px;color:#555;text-align:center;font-size:11px;font-weight:600;line-height:38px;letter-spacing:.1rem;text-transform:uppercase;text-decoration:none;white-space:nowrap;background-color:transparent;border-radius:4px;border:1px solid #bbb;cursor:pointer;box-sizing:border-box}ol,td:first-child,th:first-child,ul{padding-left:0}.button:focus,.button:hover,button:focus,button:hover,input[type=button]:focus,input[type=button]:hover,input[type=reset]:focus,input[type=reset]:hover,input[type=submit]:focus,input[type=submit]:hover{color:#333;border-color:#888;outline:0}.button.button-primary,button.button-primary,input[type=button].button-primary,input[type=reset].button-primary,input[type=submit].button-primary{color:#fff;background-color:#33c3f0;border-color:#33c3f0}.button.button-primary:focus,.button.button-primary:hover,button.button-primary:focus,button.button-primary:hover,input[type=button].button-primary:focus,input[type=button].button-primary:hover,input[type=reset].button-primary:focus,input[type=reset].button-primary:hover,input[type=submit].button-primary:focus,input[type=submit].button-primary:hover{color:#fff;background-color:#1eaedb;border-color:#1eaedb}input[type=email],input[type=number],input[type=password],input[type=search],input[type=tel],input[type=text],input[type=url],select,textarea{height:38px;padding:6px 10px;background-color:#fff;border:1px solid #d1d1d1;border-radius:4px;box-shadow:none;box-sizing:border-box}input[type=email],input[type=number],input[type=password],input[type=search],input[type=tel],input[type=text],input[type=url],textarea{-webkit-appearance:none;-moz-appearance:none;appearance:none}textarea{min-height:65px;padding-top:6px;padding-bottom:6px}input[type=email]:focus,input[type=number]:focus,input[type=password]:focus,input[type=search]:focus,input[type=tel]:focus,input[type=text]:focus,input[type=url]:focus,select:focus,textarea:focus{border:1px solid #33c3f0;outline:0}label,legend{display:block;margin-bottom:.5rem;font-weight:600}fieldset{padding:0;border-width:0}input[type=checkbox],input[type=radio]{display:inline}label>.label-body{display:inline-block;margin-left:.5rem;font-weight:400}ul{list-style:circle inside}ol{list-style:decimal inside}ol ol,ol ul,ul ol,ul ul{margin:1.5rem 0 1.5rem 3rem;font-size:90%}.button,button,li{margin-bottom:1rem}code{padding:.2rem .5rem;margin:0 .2rem;font-size:90%;white-space:nowrap;background:#f1f1f1;border:1px solid #e1e1e1;border-radius:4px}pre>code{display:block;padding:1rem 1.5rem;white-space:pre}td,th{padding:12px 15px;text-align:left;border-bottom:1px solid #e1e1e1}td:last-child,th:last-child{padding-right:0}fieldset,input,select,textarea{margin-bottom:1.5rem}blockquote,dl,figure,form,ol,p,pre,table,ul{margin-bottom:2.5rem}.u-full-width{width:100%;box-sizing:border-box}.u-max-full-width{max-width:100%;box-sizing:border-box}.u-pull-right{float:right}.u-pull-left{float:left}hr{margin-top:3rem;margin-bottom:3.5rem;border-width:0;border-top:1px solid #e1e1e1}.container:after,.row:after,.u-cf{content:"";display:table;clear:both}
        .obsolete { text-decoration: line-through; }
        .status-dot { width: 10px; height: 10px; border-radius: 50%; display: inline-block; }
        .status-dot.seeding { background-color: yellow; }
    </style>
    <script src="//cdn.jsdelivr.net/npm/chart.js"></script>
</head>
<body>
<div class="container">
`

	footer := `
</div>
	</body>
</html>
`

	templates := map[string]string{
		"root": header + `
	<section class="header">
		<h1 class="title">Anna's Archive Mirror</h1>
	  </section>
	  <p><strong>This is a mirror of the <a href="https://annas-archive.org/torrents">Anna's Archive torrent page</a></strong>. We strive to keep everything as up-to-date as possible, however, we are not affiliated with Anna's Archive and this list is maintained privately.</p>
	  <p>This torrent list is the “ultimate unified list” of releases by Anna’s Archive, Library Genesis, Sci-Hub, and others. By seeding these torrents, you help preserve humanity’s knowledge and culture. These torrents represent the vast majority of human knowledge that can be mirrored in bulk.</p>
	  <p>These torrents are not meant for downloading individual books. They are meant for long-term preservation. With these torrents you can set up a full mirror of Anna’s Archive, using their <a href="https://software.annas-archive.se/AnnaArchivist/annas-archive">source code</a> and metadata (which can be <a href="https://software.annas-archive.se/AnnaArchivist/annas-archive/-/blob/main/data-imports/README.md">generated</a> or <a href="https://annas-archive.org/torrents#aa_derived_mirror_metadata">downloaded</a> as ElasticSearch and MariaDB databases). We scrape the <a href="https://annas-archive.org/dyn/torrents.json">full torrent list</a> from Anna's Archive every 24 hours. We also have full lists of torrents, as JSON, available <a href="/json">here</a>.</p>
	  <canvas id="seederStatsChart" width="800" height="400"></canvas>
	  <script>
		  async function fetchData() {
			  const response = await fetch('/seeder-stats');
			  return await response.json();
		  }
  
  async function createChart() {
	  const data = await fetchData();
	  const ctx = document.getElementById('seederStatsChart').getContext('2d');
	  
	  new Chart(ctx, {
		  type: 'line',
		  data: {
			  labels: data.map(d => d.date),
			  datasets: [
				  {
					  label: '<4 seeders',
					  data: data.map(d => d.lowSeedersTB),
					  backgroundColor: 'rgba(255, 99, 132, 0.7)',
					  borderColor: 'rgba(255, 99, 132, 1)',
					  fill: true,
					  order: 3
				  },
				  {
					  label: '4-10 seeders',
					  data: data.map(d => d.mediumSeedersTB),
					  backgroundColor: 'rgba(255, 206, 86, 0.7)',
					  borderColor: 'rgba(255, 206, 86, 1)',
					  fill: true,
					  order: 2
				  },
				  {
					  label: '>10 seeders',
					  data: data.map(d => d.highSeedersTB),
					  backgroundColor: 'rgba(75, 192, 192, 0.7)',
					  borderColor: 'rgba(75, 192, 192, 1)',
					  fill: true,
					  order: 1
				  }
			  ]
		  },
		  options: {
			  responsive: true,
			  scales: {
				  x: {
					  type: 'category',
					  title: {
						  display: true,
						  text: 'Date'
					  }
				  },
				  y: {
					  stacked: true,
					  title: {
						  display: true,
						  text: 'Terabytes'
					  },
					  ticks: {
						  callback: function(value) {
							  return value + 'TB';
						  }
					  }
				  }
			  },
			  plugins: {
				  title: {
					  display: true,
					  text: 'Daily Seeder Stats'
				  },
				  legend: {
					  display: true,
					  position: 'top'
				  },
				  tooltip: {
					  mode: 'index',
					  intersect: false
				  }
			  },
			  interaction: {
				  mode: 'nearest',
				  axis: 'x',
				  intersect: false
			  }
		  }
	  });
  }
  
		  createChart();
	  </script>
		 <form action="/generate-torrent-list" method="POST" target="_blank">
			<div class="row">
				<div class="four columns">
					<label for="maxTB">Max TB:</label>
					<input type="number" id="maxTB" name="maxTB" step="0.1" min="0" required>
				</div>
				<div class="four columns">
					<label for="listType">Type:</label>
					<select id="listType" name="listType" required>
						<option value="magnet">Magnet links</option>
						<option value="torrent">Torrent files</option>
						<option value="both">Both</option>
					</select>
				</div>
				<div class="four columns">
					<label for="format">Format:</label>
					<select id="format" name="format" required>
						<option value="json">JSON</option>
						<option value="xml">XML</option>
						<option value="txt">TXT</option>
					</select>
				</div>
			</div>
			<div class="row">
				<div class="twelve columns" style="text-align: right;">
					<input class="button-primary" type="submit" value="Generate">
				</div>
			</div>
		</form>
	{{range .}}
	<h2>{{.TopLevelGroupName}}</h2>
	<h3>{{.GroupName}}</h3>
	<table>
		<tr>
			<th>Torrent Name</th>
			<th>Date Added</th>
			<th>Data Size</th>
			<th>Num Files</th>
			<th>Type</th>
			<th>Status</th>
			<th>Magnet Link</th>
			<th>Torrent Link</th>
		</tr>
		{{range .Torrents}}
		<tr>
			<td class="{{if .Obsolete}}obsolete{{end}}"><a href="/stats/{{.BTIH}}">{{.DisplayName}}</a></td>
			<td>{{.FormattedAddedDate}}</td>
			<td>{{.FormattedDataSize}}</td>
			<td>{{.NumFiles}}</td>
			<td>{{.MetadataLabel}}</td>
			<td>{{.StatusLabel}}</td>
			<td><a href="{{.MagnetLink | urlsafe}}">Magnet</a></td>
			<td>
				{{if torrentsEnabled}}
					<a href="/assets/torrents/{{.BTIH}}.torrent">Torrent</a>
				{{else}}
					<a href="{{.URL}}">Torrent</a>
				{{end}}
			</td>
		</tr>
		{{end}}
	</table>
	{{if gt .TotalCount 20}}
	<a href="/full/{{.GroupName}}">View full list</a>
	{{end}}
	{{end}}
	` + footer,

		"fullList": header + `
	<h1>{{.GroupName}} - Full List</h1>
	<table>
		<tr>
			<th>Torrent Name</th>
			<th>Date Added</th>
			<th>Data Size</th>
			<th>Num Files</th>
			<th>Type</th>
			<th>Status</th>
			<th>Magnet Link</th>
			<th>Torrent Link</th>
		</tr>
		{{range .Torrents}}
		<tr>
			<td class="{{if .Obsolete}}obsolete{{end}}"><a href="/stats/{{.BTIH}}">{{.DisplayName}}</a></td>
			<td>{{.FormattedAddedDate}}</td>
			<td>{{.FormattedDataSize}}</td>
			<td>{{.NumFiles}}</td>
			<td>{{.MetadataLabel}}</td>
			<td>{{.StatusLabel}}</td>
			<td><a href="{{.MagnetLink | urlsafe}}">Magnet</a></td>
			<td>
				{{if torrentsEnabled}}
					<a href="/assets/torrents/{{.BTIH}}.torrent">Torrent</a>
				{{else}}
					<a href="{{.URL}}">Torrent</a>
				{{end}}
			</td>
		</tr>
		{{end}}
	</table>
	<a href="/">Back to main page</a>
	` + footer,

		"stats": header + `
	<h1>Torrent Stats: {{.DisplayName}}</h1>
	<table>
		<tr>
			<th>Property</th>
			<th>Value</th>
		</tr>
		<tr><td>Added To Torrents List</td><td>{{.AddedToTorrentsList}}</td></tr>
		<tr><td>Seeders</td><td>{{.FormattedSeeders}}</td></tr>
		<tr><td>Leechers</td><td>{{.FormattedLeechers}}</td></tr>
		<tr><td>Completed</td><td>{{.Completed}}</td></tr>
		<tr><td>Size</td><td>{{.FormattedDataSize}}</td></tr>
		<tr><td>Magnet Link</td><td><a href="{{.MagnetLink | urlsafe}}">Magnet</a></td></tr>
		<tr><td>Torrent Link</td><td>
			{{if torrentsEnabled}}
					<a href="/assets/torrents/{{.BTIH}}.torrent">Torrent</a>
				{{else}}
					<a href="{{.URL}}">Torrent</a>
				{{end}}
		</td></tr>
		<tr><td>Last Update</td><td>{{.StatsScrapedAt}}</td></tr>
	</table>
	<h2>Seeder History</h2>
<canvas id="seederChart" width="800" height="400"></canvas>

<script>
	var ctx = document.getElementById('seederChart').getContext('2d');
	var seederData = {{.SeederStats | json}};

	// Find the maximum value in the data
var maxYValue = Math.max(...seederData.map(d => d.seeders));

// Calculate the maximum Y value for the scale, with padding of 10
var suggestedMax = Math.ceil(maxYValue / 10) * 10 + 10;

new Chart(ctx, {
	type: 'line',
	data: {
		labels: seederData.map(d => d.date), 
		datasets: [{
			label: 'Average Daily Seeders',
			data: seederData.map(d => d.seeders),
			borderColor: 'rgb(75, 192, 192)',
			tension: 0.1
		}]
	},
	options: {
		responsive: true,
		scales: {
			x: {
				type: 'category',
				title: {
					display: true,
					text: 'Date'
				}
			},
			y: {
				beginAtZero: true,
				suggestedMin: 0,
				suggestedMax: suggestedMax, // Set the suggested maximum value
				ticks: {
					stepSize: 1, // Ensure whole numbers
					callback: function(value) {
						return Number.isInteger(value) ? value : null;
					}
				},
				title: {
					display: true,
					text: 'Average Seeders'
				}
			}
		},
		plugins: {
			title: {
				display: true,
				text: 'Daily Average Seeder Count'
			}
		}
	}
});

</script>
<a href="/">Back to main page</a>
	` + footer,
	}

	return templates[name]
}

func formatDate(dateStr string) string {
	if len(dateStr) >= 10 {
		return dateStr[:10]
	}
	return dateStr
}
func formatTimeSince(dateStr string) string {
	// First try parsing the extended format with fractional seconds and time zone offset
	layoutWithTimezone := "2006-01-02T15:04:05.999999999Z07:00"
	t, err := time.Parse(layoutWithTimezone, dateStr)
	if err != nil {
		// If it fails, fall back to the standard format without timezone offset
		layoutWithoutTimezone := "2006-01-02T15:04:05Z"
		t, err = time.Parse(layoutWithoutTimezone, dateStr)
		if err != nil {
			// If both parsings fail, return the original string
			return dateStr
		}
	}

	now := time.Now()
	diff := now.Sub(t)

	var unit string
	var value int64
	switch {
	case diff < time.Minute:
		unit = "s"
		value = int64(diff.Seconds())
	case diff < time.Hour:
		unit = "m"
		value = int64(diff.Minutes())
	case diff < 24*time.Hour:
		unit = "h"
		value = int64(diff.Hours())
	default:
		unit = "d"
		value = int64(diff.Hours() / 24)
	}

	return fmt.Sprintf("%d%s", value, unit)
}
func formatDataSize(size int64) string {
	sizeMB := float64(size) / (1024 * 1024)
	if sizeMB > 1024 {
		sizeGB := sizeMB / 1024
		if sizeGB > 1024 {
			return fmt.Sprintf("%.2f TB", sizeGB/1024)
		}
		return fmt.Sprintf("%.2f GB", sizeGB)
	}
	return fmt.Sprintf("%.2f MB", sizeMB)
}

func formatStatus(currentlySeeding bool, seeders, leechers int, scrapedAtStr string) template.HTML {
	status := "⚫"
	switch {
	case seeders < 2:
		status = "🔴"
	case seeders > 10:
		status = "🟢"
	default:
		status = "🟡"
	}

	archived := ""
	if currentlySeeding {
		archived = "<span class='aaseeded' title=\"Seeded by Anna's Archive\">(📓)</span>"
	}

	timeSince := formatTimeSince(scrapedAtStr)
	return template.HTML(fmt.Sprintf("%s %d seed / %d leech %s <span class='timeSince'>%s</span>", status, seeders, leechers, archived, timeSince))
}

func formatNumber(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%dK", n/1000)
}

func generateTorrentList(maxTB float64, listType string) ([]map[string]interface{}, error) {
	maxBytes := int64(maxTB * 1024 * 1024 * 1024 * 1024) // Convert TB to bytes
	var totalBytes int64
	var result []map[string]interface{}

	err := withReadTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`
            SELECT btih, magnet_link, url, data_size, seeders
            FROM torrents
            WHERE obsolete = 0 AND embargo = 0 AND seeders > 0
            ORDER BY seeders ASC, data_size DESC
        `)
		if err != nil {
			return fmt.Errorf("error querying database: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var btih, magnetLink, url string
			var dataSize, seeders int64
			err := rows.Scan(&btih, &magnetLink, &url, &dataSize, &seeders)
			if err != nil {
				return fmt.Errorf("error scanning row: %w", err)
			}

			if totalBytes+dataSize > maxBytes {
				break
			}

			torrent := map[string]interface{}{
				"btih":      btih,
				"data_size": dataSize,
				"seeders":   seeders,
			}

			switch listType {
			case "magnet":
				torrent["magnet_link"] = magnetLink
			case "torrent":
				torrent["torrent_url"] = url
			case "both":
				torrent["magnet_link"] = magnetLink
				torrent["torrent_url"] = url
			}

			result = append(result, torrent)
			totalBytes += dataSize
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("error after iterating rows: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error generating torrent list: %w", err)
	}

	return result, nil
}

// convertToXML converts the torrent list to XML format
func convertToXML(torrents []map[string]interface{}) ([]byte, error) {
	type Torrent struct {
		BTIH       string `xml:"btih"`
		DataSize   int64  `xml:"data_size"`
		Seeders    int64  `xml:"seeders"`
		MagnetLink string `xml:"magnet_link,omitempty"`
		TorrentURL string `xml:"torrent_url,omitempty"`
	}

	type Torrents struct {
		XMLName xml.Name  `xml:"torrents"`
		List    []Torrent `xml:"torrent"`
	}

	var xmlTorrents []Torrent
	for _, t := range torrents {
		torrent := Torrent{
			BTIH:       getStringValue(t, "btih"),
			DataSize:   getInt64Value(t, "data_size"),
			Seeders:    getInt64Value(t, "seeders"),
			MagnetLink: getStringValue(t, "magnet_link"),
			TorrentURL: getStringValue(t, "torrent_url"),
		}
		xmlTorrents = append(xmlTorrents, torrent)
	}

	output, err := xml.MarshalIndent(Torrents{List: xmlTorrents}, "", "  ")
	if err != nil {
		return nil, err
	}

	return append([]byte(xml.Header), output...), nil
}

// Helper function to safely get a string value from the map
func getStringValue(m map[string]interface{}, key string) string {
	if value, ok := m[key].(string); ok {
		return value
	}
	return ""
}

// Helper function to safely get an int64 value from the map
func getInt64Value(m map[string]interface{}, key string) int64 {
	if value, ok := m[key].(int64); ok {
		return value
	}
	return 0
}

// convertToTXT converts the torrent list to plain text format
func convertToTXT(torrents []map[string]interface{}) string {
	var buffer bytes.Buffer
	for _, t := range torrents {

		if magnetLink, ok := t["magnet_link"]; ok {
			buffer.WriteString(fmt.Sprintf("%s\n", magnetLink.(string)))
		}
		if torrentURL, ok := t["torrent_url"]; ok {
			buffer.WriteString(fmt.Sprintf("%s\n", torrentURL.(string)))
		}
		buffer.WriteString("\n")
	}
	return buffer.String()
}
