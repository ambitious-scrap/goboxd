# #!/bin/bash
# # Runs the flask server using gunicorn to a socket
# # usage: ./gunicorn.sh [number of workers]
# # Socket: /tmp/nsjail-programming-server.sock
# no_of_workers=$1
# if [ -z $no_of_workers ]
# then
#   no_of_workers=3
# fi
# gunicorn --workers $no_of_workers --bind unix:/tmp/nsjail-programming-server.sock -m 77 wsgi

import multiprocessing

import gunicorn.app.base
from gunicorn.six import iteritems

from nsjps_flask.server import app


def number_of_workers():
    return (multiprocessing.cpu_count() * 2) + 1


class CodeServer(gunicorn.app.base.BaseApplication):
    def __init__(self, app, options=None):
        self.options = options or {}
        self.application = app
        super().__init__()

    def load_config(self):
        config = dict(
            [
                (key, value)
                for key, value in iteritems(self.options)
                if key in self.cfg.settings and value is not None
            ],
        )
        for key, value in iteritems(config):
            self.cfg.set(key.lower(), value)

    def load(self):
        return self.application


if __name__ == "__main__":
    options = {
        "bind": "unix:/tmp/nsjail-programming-server.sock",
        "mode": "77",
        "workers": number_of_workers(),
    }
    CodeServer(app, options).run()
