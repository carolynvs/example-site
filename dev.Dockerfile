FROM klakegg/hugo:ext-alpine

# Avoid collision with HUGO_CACHEDIR
ENV GOPATH=/go
RUN mkdir /go

# Cache hugo modules
COPY go.* /src/
RUN go mod download && \
    hugo mod get && \
    mv /go/pkg /tmp/modules/filecache/modules/

CMD [ "server" ]
