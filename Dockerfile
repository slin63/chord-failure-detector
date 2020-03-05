FROM golang:alpine
ENV CONFIG="config.json"
ENV INTRODUCER=false

RUN mkdir /app
ADD . /app/
WORKDIR /app
RUN go build -o main ./cmd/fd

## TODO: have this start up the remote logger as well
CMD ["sh", "-c", "./main"]
