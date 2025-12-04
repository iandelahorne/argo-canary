FROM docker.io/library/golang:1.25 AS builder

WORKDIR /go/src/github.com/iandelahorne/argo-canary

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o argo-canary cmd/main.go

FROM gcr.io/distroless/static-debian11

COPY --from=builder /go/src/github.com/iandelahorne/argo-canary/argo-canary /bin

# Use numeric user, allows kubernetes to identify this user as being
# non-root when we use a security context with runAsNonRoot: true
USER 999

WORKDIR /home/argo-canary

ENTRYPOINT [ "/bin/argo-canary" ]


