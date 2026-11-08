.PHONY : all windows linux build-linux

all : build-linux duplicacy-fuse.exe

windows : duplicacy-fuse.exe

linux : build-linux

build-linux :
	env CGO_ENABLED=1 GOOS=linux go build -o duplicacy-fuse .

duplicacy-fuse.exe : *.go
	env GOOS=windows go build .
