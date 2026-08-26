#!/usr/bin/env python3
"""Verify the cross-language Canonical JSON and Plugin hash golden fixtures."""

from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path


MAX_NUMBER_CHARACTERS = 10_240
MAX_NUMBER_EXPANSION = 10_240
MAX_NESTING = 512

sys.setrecursionlimit(4_096)


class ContractError(ValueError):
    pass


class Number:
    def __init__(self, raw: str) -> None:
        self.raw = raw


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def object_from_pairs(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ContractError(f"duplicate object key {key!r}")
        result[key] = value
    return result


def reject_constant(value: str) -> object:
    raise ContractError(f"invalid JSON number {value}")


def parse_json(raw: str) -> object:
    value = json.loads(
        raw,
        parse_int=Number,
        parse_float=Number,
        parse_constant=reject_constant,
        object_pairs_hook=object_from_pairs,
    )
    reject_surrogates(value)
    return value


def reject_surrogates(value: object) -> None:
    if isinstance(value, str):
        if any(0xD800 <= ord(character) <= 0xDFFF for character in value):
            raise ContractError("isolated UTF-16 surrogate")
    elif isinstance(value, list):
        for item in value:
            reject_surrogates(item)
    elif isinstance(value, dict):
        for key, item in value.items():
            reject_surrogates(key)
            reject_surrogates(item)


def canonical_number(raw: str) -> str:
    if len(raw) > MAX_NUMBER_CHARACTERS:
        raise ContractError("JSON number is too long")
    negative = raw.startswith("-")
    value = raw[1:] if negative else raw
    exponent = 0
    exponent_at = next((index for index, character in enumerate(value) if character in "eE"), -1)
    if exponent_at >= 0:
        exponent = int(value[exponent_at + 1 :])
        if exponent < -10_000 or exponent > 10_000:
            raise ContractError("JSON number exponent is outside the supported range")
        value = value[:exponent_at]
    fraction_digits = 0
    if "." in value:
        point = value.index(".")
        fraction_digits = len(value) - point - 1
        value = value[:point] + value[point + 1 :]
    value = value.lstrip("0")
    if not value:
        return "0"
    scale = fraction_digits - exponent
    while scale > 0 and value.endswith("0"):
        value = value[:-1]
        scale -= 1
    if scale <= 0:
        normalized = value + "0" * -scale
    elif scale >= len(value):
        normalized = "0." + "0" * (scale - len(value)) + value
    else:
        normalized = value[:-scale] + "." + value[-scale:]
    if negative:
        normalized = "-" + normalized
    if len(normalized) > MAX_NUMBER_CHARACTERS:
        raise ContractError("canonical JSON number is too long")
    return normalized


def quote(value: str) -> str:
    encoded = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    for character, escape in (
        ("<", r"\u003c"),
        (">", r"\u003e"),
        ("&", r"\u0026"),
        ("\u2028", r"\u2028"),
        ("\u2029", r"\u2029"),
    ):
        encoded = encoded.replace(character, escape)
    return encoded


def canonical_json(value: object) -> str:
    expansion = [0]

    def encode(current: object, depth: int) -> str:
        if current is None:
            return "null"
        if current is True:
            return "true"
        if current is False:
            return "false"
        if isinstance(current, str):
            return quote(current)
        if isinstance(current, Number):
            normalized = canonical_number(current.raw)
            growth = len(normalized) - len(current.raw)
            if growth > 0:
                expansion[0] += growth
                if expansion[0] > MAX_NUMBER_EXPANSION:
                    raise ContractError("canonical JSON number expansion exceeds 10240 characters")
            return normalized
        if isinstance(current, list):
            if depth >= MAX_NESTING:
                raise ContractError("JSON nesting exceeds 512 containers")
            return "[" + ",".join(encode(item, depth + 1) for item in current) + "]"
        if isinstance(current, dict):
            if depth >= MAX_NESTING:
                raise ContractError("JSON nesting exceeds 512 containers")
            return "{" + ",".join(
                quote(key) + ":" + encode(current[key], depth + 1)
                for key in sorted(current)
            ) + "}"
        raise ContractError(f"unsupported JSON value {type(current).__name__}")

    return encode(value, 0)


def canonical_package(value: object) -> str:
    if isinstance(value, dict) and isinstance(value.get("attachments"), list):
        value = dict(value)
        value["attachments"] = sorted(
            value["attachments"],
            key=lambda item: item.get("path", "") if isinstance(item, dict) else "",
        )
    return canonical_json(value)


def load_fixture(path: Path) -> object:
    return parse_json(path.read_text(encoding="utf-8"))


def main() -> None:
    root = Path(__file__).resolve().parents[1]
    canonical_fixture = load_fixture(root / "contracts/v2/fixtures/golden/canonical-json.json")
    require(isinstance(canonical_fixture, dict), "canonical fixture must be an object")
    for field, expected in (
        ("max_nesting", MAX_NESTING),
        ("max_number_characters", MAX_NUMBER_CHARACTERS),
        ("max_total_number_expansion", MAX_NUMBER_EXPANSION),
    ):
        value = canonical_fixture[field]
        require(isinstance(value, Number) and int(value.raw) == expected, f"{field} drifted")
    for case in canonical_fixture["cases"]:
        require(canonical_json(parse_json(case["input"])) == case["canonical"], case["name"])
    for raw in canonical_fixture["invalid_inputs"]:
        try:
            canonical_json(parse_json(raw))
        except (ContractError, ValueError):
            continue
        raise AssertionError(f"invalid Canonical JSON input was accepted: {raw}")
    canonical_json(parse_json("[" * MAX_NESTING + "0" + "]" * MAX_NESTING))
    try:
        canonical_json(parse_json("[" * (MAX_NESTING + 1) + "0" + "]" * (MAX_NESTING + 1)))
    except ContractError:
        pass
    else:
        raise AssertionError("Canonical JSON accepted nesting above the contract limit")

    hash_fixture = load_fixture(root / "contracts/v2/fixtures/golden/plugin-hash.json")
    require(isinstance(hash_fixture, dict), "hash fixture must be an object")
    for case in hash_fixture["cases"]:
        manifest = canonical_json(case["manifest_json"])
        package = canonical_package(case["plugin_json"])
        actual = "sha256:" + hashlib.sha256((manifest + package).encode()).hexdigest()
        require(actual == case["plugin_hash"], case["name"])
    print("cross-language golden verification passed (Python stdlib)")


if __name__ == "__main__":
    main()
