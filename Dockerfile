FROM golang:alpine
ENV CONFIG="config.json"
ENV INTRODUCER=false

RUN mkdir /app
ADD . /app/
WORKDIR /app
RUN go build -o main src/main.go

CMD ["sh", "-c", "./main"]
