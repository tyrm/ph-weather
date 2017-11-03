FROM scratch
LABEL maintainer="tyr@pettingzoo.co"

EXPOSE 8080

ADD main /
CMD ["/main"]
