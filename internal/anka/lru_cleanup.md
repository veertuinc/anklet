# LRU Cleanup

We need to be able to clean up templates that are Least Recently Used (LRU) if the incoming template has no available space on the host.

- fail because the template is too big to pull (larger than the disk)
- if the buffer is too restrictive, we fail the pull early because we don't have enough space and never will
- we cannot clean up any templates to free enough space because:
    - no templates to clean
        - Testing
            - m1-6c14r-40gb-1 job
            - no existing templates on the host
            - Set your `template_disk_buffer` to a percentage so the `afterFreeingUsableSpace` is slightly lower than the `downloadSize`, but a small amount of `usableSpace` so it tries to clean up templates
    - even if we cleaned up all templates, we still don't have enough space
        - Testing
            - m1-6c14r-40gb-1 job
            - one template
                - anka -j registry pull --shrink anklet-3 --tag lru-test-1
            - Set your `template_disk_buffer` to a percentage so the `afterFreeingUsableSpace` is slightly lower than the `downloadSize`, but you also don't want to hit the buffer limit
- if we have the same template, but a different tag, we should calculate the non-cached size of the different tag with what's incoming and if it's enough, just pull with --shrink (default) (PENDING: https://veertu.atlassian.net/browse/ANKA-7434)
