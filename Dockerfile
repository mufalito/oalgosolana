# --- build stage ---
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /solanapay ./cmd/server

# --- run stage ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /solanapay /solanapay
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/solanapay"]
