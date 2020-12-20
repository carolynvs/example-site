FROM klakegg/hugo:ext-alpine

# Cache go modules
COPY go.* /src/
RUN go mod download

CMD [ "server" ]
