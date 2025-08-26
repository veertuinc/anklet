# LRU Cleanup

We need to be able to clean up templates that are Least Recently Used (LRU) if the incoming template has no available space on the host.

- fail because the template is too big to pull (larger than the disk)
- we cannot clean up any templates to free enough space because:
    - no templates to clean
    - even if we cleaned up all templates, we still don't have enough space
- if we have the same template, but a different tag, we should calculate the non-cached size of the different tag with what's incoming and if it's enough, just pull with --shrink (default) (PENDING: https://veertu.atlassian.net/browse/ANKA-7434)
