FROM dockerfile/python-runtime
ADD . /app
CMD ["/env/bin/python", "/app/health.py"]
