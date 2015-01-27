FROM scratch

ADD bin/health /opt/health
CMD ["/opt/health"]
