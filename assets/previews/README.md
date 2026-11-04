# Re-recording the README preview

The preview is recorded with [VHS](https://github.com/charmbracelet/vhs) in an anonymous demo project.

```sh
python3 make_demo.py                       # builds /var/tmp/seek-demo (a small JavaScript project)
cd /var/tmp/seek-demo && mkdir -p tapes && cp <this folder>/*.tape tapes/ && cd tapes
vhs hero.tape
cp hero.gif <repo>/assets/
rm -rf /var/tmp/seek-demo
```

Needs `vhs` (with `ttyd` and `ffmpeg`), `rg`, git and a JetBrainsMono Nerd Font. `common.tape` sets a fake `HOME`,
so no real user or path ends up in the image.
