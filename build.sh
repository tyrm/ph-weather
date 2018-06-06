#!/bin/bash

go get github.com/dchest/uniuri
go get github.com/go-redis/redis
go get github.com/google/jsonapi

CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

