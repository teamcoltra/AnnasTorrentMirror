#!/bin/bash

set -e

# Default values
DEFAULT_PORT=8080
DEFAULT_DIRECTORY="/var/annas-torrents"

# Function to install Go
install_go() {
    echo "Installing Go..."
    GO_VERSION="1.23.0"  # Use the specific version you need
    ARCH=$(uname -m)

    case $ARCH in
        x86_64) ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
    esac

    GO_TARBALL="go${GO_VERSION}.linux-${ARCH}.tar.gz"
    wget "https://go.dev/dl/${GO_TARBALL}"
    sudo tar -C /usr/local -xzf "${GO_TARBALL}"
    rm "${GO_TARBALL}"
    
    export PATH=$PATH:/usr/local/go/bin
    echo "export PATH=\$PATH:/usr/local/go/bin" >> ~/.bashrc
}

# Function to install GCC
install_gcc() {
    echo "Installing GCC..."
    if [ -x "$(command -v apt-get)" ]; then
        sudo apt-get update
        sudo apt-get install -y build-essential
    elif [ -x "$(command -v yum)" ]; then
        sudo yum groupinstall -y "Development Tools"
    elif [ -x "$(command -v zypper)" ]; then
        sudo zypper install -t pattern devel_basis
    elif [ -x "$(command -v pacman)" ]; then
        sudo pacman -Sy --noconfirm base-devel
    else
        echo "Unsupported package manager. Please install GCC manually."
        exit 1
    fi
}

# Function to set up directory
setup_directory() {
    echo "Setting up directory..."
    sudo mkdir -p "$INSTALL_DIRECTORY"
    sudo chown -R www-data:www-data "$INSTALL_DIRECTORY"
    sudo chmod -R 755 "$INSTALL_DIRECTORY"
}

# Prompt for port
read -p "Enter the port you want to use (default is $DEFAULT_PORT): " PORT
PORT=${PORT:-$DEFAULT_PORT}

# Prompt for installation directory
read -p "Enter the installation directory (default is $DEFAULT_DIRECTORY): " INSTALL_DIRECTORY
INSTALL_DIRECTORY=${INSTALL_DIRECTORY:-$DEFAULT_DIRECTORY}

# Prompt for hosting torrents locally
read -p "Do you want to host torrent files locally? (y/n, default is n): " HOST_TORRENTS
HOST_TORRENTS=${HOST_TORRENTS:-n}
TORRENT_FLAG=""
if [[ "$HOST_TORRENTS" =~ ^[Yy]$ ]]; then
    TORRENT_FLAG="--torrents true"
fi

# Check if Go is installed
if ! command -v go &> /dev/null; then
    install_go
else
    echo "Go is already installed."
fi

# Check if GCC is installed
if ! command -v gcc &> /dev/null; then
    install_gcc
else
    echo "GCC is already installed."
fi

# Install Go dependencies
echo "Installing Go dependencies..."
go get github.com/mattn/go-sqlite3
go get github.com/etix/goscrape

# Set up the directory with correct permissions
setup_directory

# Build the application
echo "Building the application..."
go build -o /usr/bin/annas-torrents

# If port 80 is selected, ensure the binary can bind to it
if [ "$PORT" -eq 80 ]; then
    echo "You selected port 80. Configuring permissions..."
    sudo setcap 'cap_net_bind_service=+ep' /usr/bin/annas-torrents
fi

# Create systemd service file
echo "Creating systemd service..."
SERVICE_FILE="/etc/systemd/system/annas-torrents.service"

sudo bash -c "cat > $SERVICE_FILE" <<EOL
[Unit]
Description=Anna's Torrents Service
After=network.target

[Service]
ExecStart=/usr/bin/annas-torrents --port $PORT --directory $INSTALL_DIRECTORY $TORRENT_FLAG
User=www-data
Restart=always

[Install]
WantedBy=multi-user.target
EOL

# Reload systemd, enable, and start the service
sudo systemctl daemon-reload
sudo systemctl enable annas-torrents.service
sudo systemctl start annas-torrents.service

echo "Installation and setup complete. Anna's Torrents is now running on port $PORT with files stored in $INSTALL_DIRECTORY."
if [[ "$HOST_TORRENTS" =~ ^[Yy]$ ]]; then
    echo "Torrent files are being hosted locally."
fi
