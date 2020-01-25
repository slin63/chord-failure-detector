FROM golang:alpine
ENV CONFIG="config.json"

RUN mkdir /app
ADD . /app/
WORKDIR /app
RUN go build -o main src/main.go

## TODO: have this start up the remote logger as well
CMD ["sh", "-c", "./main -server -generate --logname test.log"]
