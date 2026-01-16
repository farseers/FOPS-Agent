#mac
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o ../dist/fops-agent.Darwin.x86_64 -ldflags="-w -s" .
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o ../dist/fops-agent.Darwin.arm64 -ldflags="-w -s" .
    
#linux64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ../dist/fops-agent.Linux.x86_64 -ldflags="-w -s" .
GOOS=linux GOARCH=386 CGO_ENABLED=0 go build -o ../dist/fops-agent.Linux.i686 -ldflags="-w -s" .
#windows
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o ../dist/fops-agent.Windows.x86_64 -ldflags="-w -s" .
GOOS=windows GOARCH=386 CGO_ENABLED=0 go build -o ../dist/fops-agent.Windows.i686 -ldflags="-w -s" .