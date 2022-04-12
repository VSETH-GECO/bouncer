FROM golang:1.18-alpine AS build

WORKDIR /app

RUN go install github.com/kevinburke/go-bindata/go-bindata@latest

COPY go.mod ./
COPY go.sum ./
RUN go mod download

ADD cmd ./cmd
ADD migrations ./migrations
ADD pkg ./pkg

RUN go generate ./migrations
RUN go build -o /bouncer ./cmd

FROM scratch

COPY --from=build /bin/sh /bin/sh
COPY --from=build /bin/mkdir /bin/mkdir
COPY --from=build /lib /lib
COPY --from=build /etc /etc
COPY --from=build /bouncer /bouncer

RUN mkdir /etc/bouncer

EXPOSE 2112

ENTRYPOINT [ "/bouncer" ]
