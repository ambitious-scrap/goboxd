/* Native fork bomb — kernel cgroup_pids_max must contain it. */
#include <unistd.h>
int main(void) {
    while (1) { (void)fork(); }
    return 0;
}
