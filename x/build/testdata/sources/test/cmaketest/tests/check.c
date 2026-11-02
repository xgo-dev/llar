#include <stdio.h>
#include "cmtadd.h"

int main(void) {
    int r = cmt_add(2, 3);
    if (r != 5) {
        fprintf(stderr, "cmt_add(2, 3) = %d, want 5\n", r);
        return 1;
    }
    printf("cmtadd_check OK\n");
    return 0;
}
