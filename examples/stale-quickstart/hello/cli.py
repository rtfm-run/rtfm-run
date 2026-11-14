import argparse


def main() -> None:
    p = argparse.ArgumentParser(prog="hello")
    p.add_argument("--name", required=True)
    args = p.parse_args()
    print(f"Hello, {args.name}!")
