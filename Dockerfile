# API image: `rtfm serve --workers 0` (runs execute on worker VMs with KVM).
FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/rtfm ./cmd/rtfm

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/rtfm /rtfm
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/rtfm"]
CMD ["serve", "--workers", "0"]
