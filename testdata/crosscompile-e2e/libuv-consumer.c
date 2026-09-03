#include <stdio.h>

#include <uv.h>

#ifndef EXPECT_ABI_MATCH
#error EXPECT_ABI_MATCH must be defined
#endif

int main(void) {
    size_t built_size = uv_loop_size();
    size_t consumer_size = sizeof(uv_loop_t);

    printf("libuv loop size: library=%zu consumer=%zu\n",
        built_size, consumer_size);

#if EXPECT_ABI_MATCH
    // The musl-built library and musl consumer must use the same pthread layout.
    if (built_size != consumer_size) {
        return 1;
    }
#else
    // The glibc consumer is the negative control: uv_loop_t contains libc-owned
    // pthread types, so matching sizes would fail to distinguish the two ABIs.
    if (built_size == consumer_size) {
        return 1;
    }
#endif
    return 0;
}
