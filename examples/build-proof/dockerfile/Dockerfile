# syntax=docker/dockerfile:1
FROM busybox:1.37.0@sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028

COPY www /www
EXPOSE 8080
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
