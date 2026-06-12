# Process flood — cgroup_pids_max + rlimit_nproc must stop it.
import os
while True:
    os.fork()
