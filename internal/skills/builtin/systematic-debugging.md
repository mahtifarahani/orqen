---
name: systematic-debugging
description: Find the cause of a failing check before you change code, one hypothesis at a time.
version: 1.0.0
roles: worker, researcher
when: fix_round
license: MIT
source: orqen-builtin; idea from obra/superpowers (MIT)
---
# Systematic debugging

You are in a fix round, so the last attempt did not pass.

1. Reproduce the failure and read the whole output. The first error usually matters more than the last.
2. Write down one hypothesis about the cause. Check it in the code before you edit anything.
3. Change one thing at a time and run the failing command again after each change.
4. When a guess is wrong, undo it. Do not stack guesses on top of each other.
5. Look at what changed since the last passing state; the cause is often in that difference.
6. Fix the cause. Do not make a check pass by weakening, skipping or deleting it.
7. In your reply, say what the cause was and how you confirmed it.
