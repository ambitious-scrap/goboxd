import os


class Config:
    DEBUG = os.environ.get("DEBUG", "False") == "True"
    JAVA_HOME = os.environ.get("JAVA_HOME", "")
    PATH = os.environ.get("PATH", "")
    HOME = os.environ.get("HOME", "")
    TEST_ROOT_DIRECTORY = os.environ.get("TEST_ROOT_DIRECTORY")
    NSJAIL_SERVER_URL = os.environ.get("NSJAIL_SERVER_URL")
    # Add more as you find them in the codebase
