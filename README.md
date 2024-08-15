# Anna's Archive Mirror

This Go application creates a mirror of the [Anna's Archive torrent page](https://annas-archive.org/torrents), providing an up-to-date list of torrents and their statistics. It includes features for tracking seeder history, generating custom torrent lists, and visualizing seeder statistics.

## How It Works

1. **Data Collection**: The application scrapes the full torrent list from Anna's Archive every 24 hours.
2. **Database**: It stores the torrent information in a SQLite database.
3. **Stat Tracking**: Every 30 minutes, it updates the seeder, leecher, and completion statistics for each torrent.
4. **Web Interface**: It provides a web interface to view the torrent list, individual torrent statistics, and seeder history.
5. **Custom List Generation**: Users can generate custom torrent lists based on size and type preferences.

## Features

- Display torrent list with group categorization
- Show detailed statistics for individual torrents
- Track and display seeder history
- Generate custom torrent lists (magnet links, torrent files, or both)
- Visualize daily seeder statistics across all torrents

## Installation
You can just go to the releases and download the binary and skip the installation / building. Obviously use your own judgement on the OpSec of downloading pre-compiled binaries. Luckily building in Go is very easy. 

1. Clone the repository:
   ```
   git clone https://github.com/yourusername/annas-archive-mirror.git
   ```

2. Navigate to the project directory:
   ```
   cd annas-archive-mirror
   ```

3. Build the application (see Building section below).

4. Run the application:
   ```
   ./annas-archive-mirror
   ```

5. Access the web interface by opening a browser and navigating to `http://localhost:8080`

## Building

This application uses CGO and requires GCC to be installed on your system. Follow these steps to build the application:

1. Install Go (version 1.16 or later) from [golang.org](https://golang.org/)

2. Install GCC:
   - On Ubuntu/Debian: `sudo apt-get install build-essentials`
   - On macOS: Install Xcode Command Line Tools
   - On Windows: Install MinGW-w64 (I didn't need to do this but other guides say you do)

3. Install the required Go packages:
   ```
   go get github.com/mattn/go-sqlite3
   go get github.com/etix/goscrape
   ```

4. Build the application:
   ```
   go build -o annas-archive-mirror
   ```

## Configuration

You can configure the application using command-line flags:

- `-port`: Set the port for the web server (default: 8080)
- `-directory`: Set the directory to store the SQLite database (default: current directory)

Example:
```
./annas-archive-mirror -port 9000 -directory /path/to/data
```

## How It Works in Detail

1. **Initialization**:
   - The application sets up a SQLite database to store torrent information.
   - It creates necessary tables for torrents, torrent updates, and daily seeder statistics.

2. **Data Collection**:
   - Every 24 hours, it fetches the latest torrent list from Anna's Archive.
   - New torrents are added to the database, and existing ones are updated.

3. **Stat Tracking**:
   - Every 30 minutes, it updates the seeder, leecher, and completion statistics for each torrent.
   - It uses the `github.com/etix/goscrape` library to scrape tracker information.

4. **Web Server**:
   - The application runs a web server that provides various endpoints:
     - `/`: Main page with grouped torrent list
     - `/full/{groupName}`: Full list of torrents for a specific group
     - `/stats/{btih}`: Detailed statistics for a specific torrent
     - `/json`: Full torrent list in JSON format
     - `/generate-torrent-list`: Endpoint for generating custom torrent lists

5. **Visualization**:
   - It uses Chart.js to create visual representations of seeder statistics.

6. **Custom List Generation**:
   - Users can specify maximum size, list type (magnet links, torrent files, or both), and output format (JSON, XML, or TXT).
   - The application generates the list based on these criteria, prioritizing torrents with fewer seeders.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is open source and available under the [MIT License](LICENSE).