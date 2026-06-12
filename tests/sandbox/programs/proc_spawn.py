# Spawn long-lived children to fill the process table.
import os, time
while True:
    if os.fork() == 0:
        time.sleep(30)
        os._exit(0)
