"""
WSGI file for gunicorn.

To use the nginx.conf provided with this project, run the following command:

```
gunicorn --workers 3 --bind unix:/tmp/nsjail-programming-server.sock -m -77 wsgi
```

You may specify any number of workers.
"""

from nsjps_flask.server import app as application


if __name__ == "__main__":
    application.run(debug=True, host="0.0.0.0", port=8000)
