# Write to /etc — directory absent in chroot; must raise FileNotFoundError.
open('/etc/hostname', 'w').write('hacked')
