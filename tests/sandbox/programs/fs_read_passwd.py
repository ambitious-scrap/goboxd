# Read /etc/passwd — path does not exist inside the chroot; must raise FileNotFoundError.
open('/etc/passwd').read()
