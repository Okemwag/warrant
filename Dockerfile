FROM node:22-alpine AS console
WORKDIR /console
COPY console/package.json console/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY console/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/warrant ./server/cmd/warrant && \
    CGO_ENABLED=0 go build -o /out/warrant-mcp ./bridge/cmd/warrant-mcp

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/warrant /out/warrant-mcp /
COPY --from=console /console/dist /console
ENV WARRANT_CONSOLE_DIR=/console
EXPOSE 8080
ENTRYPOINT ["/warrant"]
CMD ["serve", "-migrate"]
