import logging
import os


class AppLogger:
    def __init__(self):
        self.debug = os.getenv("DEBUG", "False").lower() in ("true", "1")

        self.logger = logging.getLogger("AppLogger")
        if not self.logger.handlers:
            self.logger.setLevel(logging.DEBUG if self.debug else logging.INFO)
            handler = logging.StreamHandler()
            formatter = logging.Formatter(
                "%(asctime)s | %(levelname)s | %(message)s",
            )
            handler.setFormatter(formatter)
            self.logger.addHandler(handler)
            self.logger.propagate = False

    def info(self, msg):
        if self.debug:
            self.logger.info(msg)

    def warning(self, msg):
        self.logger.warning(msg)

    def error(self, msg):
        self.logger.error(msg)


logger = AppLogger()
