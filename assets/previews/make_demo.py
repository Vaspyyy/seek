#!/usr/bin/env python3
"""Build an anonymous demo project for seek's README previews."""
import os, shutil, subprocess, textwrap

ROOT = "/var/tmp/seek-demo/home"
R = os.path.join(ROOT, "projects", "skirmish")
shutil.rmtree("/var/tmp/seek-demo", ignore_errors=True)
os.makedirs(R)


def w(name, text):
    p = os.path.join(R, name)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w") as f:
        f.write(textwrap.dedent(text).lstrip("\n"))


w("src/config.js", """
    export const RETREAT_MORALE = 0.25;
    export const RETREAT_SPEED = 1.4;
    export const UNIT_SPEED = 0.8;
""")
w("src/units.js", """
    import { RETREAT_MORALE, RETREAT_SPEED, UNIT_SPEED } from "./config.js";

    export function createUnitUpdater(world) {
    \t/**
    \t * A unit falls back when its morale breaks or it is outnumbered.
    \t * The retreat vector points away from the nearest enemies.
    \t */
    \tfunction shouldRetreat(unit) {
    \t\tconst enemies = world.enemiesNear(unit, 3);
    \t\tconst allies = world.alliesNear(unit, 3);
    \t\treturn unit.morale < RETREAT_MORALE || enemies.length > allies.length * 2;
    \t}

    \tfunction retreatVector(unit) {
    \t\tconst away = world.awayFrom(unit, world.enemiesNear(unit, 5));
    \t\treturn { x: away.x * RETREAT_SPEED, y: away.y * RETREAT_SPEED };
    \t}

    \tfunction step(unit, dt) {
    \t\tif (shouldRetreat(unit)) {
    \t\t\tunit.state = "retreat";
    \t\t\tconst v = retreatVector(unit);
    \t\t\tunit.x += v.x * dt;
    \t\t\tunit.y += v.y * dt;
    \t\t\treturn;
    \t\t}
    \t\tunit.state = "advance";
    \t\tunit.x += unit.heading.x * UNIT_SPEED * dt;
    \t\tunit.y += unit.heading.y * UNIT_SPEED * dt;
    \t}

    \treturn { step, shouldRetreat };
    }
""")
w("src/world.js", """
    export function createWorld(units) {
    \tfunction near(unit, radius, enemy) {
    \t\treturn units.filter((u) => (u.side !== unit.side) === enemy && dist(u, unit) < radius);
    \t}

    \treturn {
    \t\tenemiesNear: (unit, radius) => near(unit, radius, true),
    \t\talliesNear: (unit, radius) => near(unit, radius, false),
    \t\tawayFrom(unit, others) {
    \t\t\tif (others.length === 0) return { x: 0, y: 0 };
    \t\t\tconst cx = others.reduce((s, u) => s + u.x, 0) / others.length;
    \t\t\tconst cy = others.reduce((s, u) => s + u.y, 0) / others.length;
    \t\t\treturn normalize({ x: unit.x - cx, y: unit.y - cy });
    \t\t},
    \t};
    }

    function dist(a, b) {
    \treturn Math.hypot(a.x - b.x, a.y - b.y);
    }

    function normalize(v) {
    \tconst n = Math.hypot(v.x, v.y) || 1;
    \treturn { x: v.x / n, y: v.y / n };
    }
""")
w("src/ui.js", """
    export function bindUi(sim) {
    \tsim.worker.onmessage = (evt) => {
    \t\tconst { units } = evt.data;
    \t\tconst fleeing = units.filter((u) => u.state === "retreat").length;
    \t\tdocument.title = `${fleeing} units in retreat`;
    \t};
    \tdocument.getElementById("speed").addEventListener("change", (e) => {
    \t\tsim.setSpeed(Number(e.target.value)); // the retreat speed is fixed
    \t});
    }
""")
w("tests/units.test.js", """
    import { createUnitUpdater } from "../src/units.js";

    test("an outnumbered unit retreats", () => {
    \tconst world = fakeWorld({ enemies: 5, allies: 1 });
    \tconst u = { morale: 1, x: 0, y: 0, heading: { x: 1, y: 0 } };
    \tcreateUnitUpdater(world).step(u, 1);
    \texpect(u.state).toBe("retreat");
    });
""")
w("README.md", """
    # skirmish

    A small battle simulation. Units advance until their morale breaks or
    they are outnumbered, then retreat away from the nearest enemies.
""")
subprocess.run(["git", "init", "-q", "-b", "main"], cwd=R, check=True)
subprocess.run(["git", "add", "-A"], cwd=R, check=True)
print("demo project ready:", R)
