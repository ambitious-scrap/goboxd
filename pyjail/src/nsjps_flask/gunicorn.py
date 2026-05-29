#!/usr/bin/env python3
"""
Gunicorn production configuration for nsjps-flask.

Usage:
    python -m nsjps_flask.gunicorn
"""

import multiprocessing
import os

from gunicorn.app.wsgiapp import WSGIApplication


class StandaloneApplication(WSGIApplication):
    def __init__(self, app_uri, options=None):
        self.options = options or {}
        self.app_uri = app_uri
        super().__init__()

    def load_config(self):
        for key, value in self.options.items():
            if key in self.cfg.settings and value is not None:
                self.cfg.set(key, value)

    def load(self):
        return self.load_wsgiapp()


def get_worker_count():
    if os.environ.get("WEB_CONCURRENCY"):
        return int(os.environ.get("WEB_CONCURRENCY"))

    cpu_count = multiprocessing.cpu_count()
    # Conservative formula for CPU-intensive workloads
    return max(2, min(cpu_count, 8))


def get_gunicorn_options():
    return {
        "bind": f"0.0.0.0:{os.environ.get('PORT', '8000')}",
        "backlog": 2048,
        "workers": get_worker_count(),
        "worker_class": "sync",
        "worker_connections": 1000,
        "max_requests": 100,
        "max_requests_jitter": 20,
        "timeout": int(os.environ.get("TIMEOUT", "120")),
        "graceful_timeout": 30,
        "keepalive": 5,
        "proc_name": "nsjps",
        "accesslog": "-",
        "errorlog": "-",
        "loglevel": os.environ.get("LOG_LEVEL", "warning"),
        "access_log_format": '%(h)s %(l)s %(u)s %(t)s "%(r)s" %(s)s %(b)s "%(f)s" "%(a)s" %(D)s',
        "preload_app": True,
        "worker_tmp_dir": "/dev/shm",
    }


def main():
    options = get_gunicorn_options()
    app = StandaloneApplication("nsjps_flask.wsgi:application", options)
    app.run()


if __name__ == "__main__":
    main()
