FROM golang:alpine
ENV MACHINEID=0 CONFIG="config.json"

RUN mkdir /app
ADD . /app/
WORKDIR /app
RUN go build -o main src/main.go

CMD ["sh", "-c", "./main -server -generate --logname test.log"]
