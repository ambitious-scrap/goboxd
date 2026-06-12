# Linux threads count against rlimit_nproc — spawn until limit hit.
import threading, time
while True:
    threading.Thread(target=time.sleep, args=(60,), daemon=True).start()
