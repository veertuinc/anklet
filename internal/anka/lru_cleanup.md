# LRU Cleanup

We need to be able to clean up templates that are Least Recently Used (LRU) if the incoming template has no available space on the host.

- fail because the template is too big to pull (larger than the disk)
- if the buffer is too restrictive, we fail the pull early because we don't have enough space and never will
- we cannot clean up any templates to free enough space because:
    - no templates to clean
        - Testing
            - 8c14r job
            - only template/tag is anka -j registry pull --shrink 84266873-da90-4e0d-903b-ed0233471f9f --tag 6c14r
            - Set your `template_disk_buffer` to a percentage so the `afterFreeingUsableSpace` is slightly lower than the `downloadSize`, but also have no other templates on the host to clean up
    - even if we cleaned up all templates, we still don't have enough space
        - Testing
            - 8c14r job
            - only template/tag is anka -j registry pull --shrink 84266873-da90-4e0d-903b-ed0233471f9f --tag 6c14r
            - Set your `template_disk_buffer` to a percentage so the `afterFreeingUsableSpace` is slightly lower than the `downloadSize`, but you also don't want to hit the buffer limit
- if we have the same template, but a different tag, we should calculate the non-cached size of the different tag with what's incoming and if it's enough, just pull with --shrink (default) (PENDING: https://veertu.atlassian.net/browse/ANKA-7434)
