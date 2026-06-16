FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /mock-ws .

FROM alpine:3.21
COPY --from=build /mock-ws /mock-ws
EXPOSE 8080
CMD ["/mock-ws"]
