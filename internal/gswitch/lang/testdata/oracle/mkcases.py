"""Build oracle input lines "<shown-lang>\t<text>" from layout_cases.tsv + extras."""
import sys

EN = "1234567890-=qwertyuiop[]asdfghjkl;'`\\zxcvbnm,./"
EN_S = "!@#$%^&*()_+QWERTYUIOP{}ASDFGHJKL:\"~|ZXCVBNM<>?"
RU = "1234567890-=йцукенгшщзхъфывапролджэё\\ячсмитьбю."
RU_S = "!\"№;%:?*()_+ЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЁ/ЯЧСМИТЬБЮ,"


def convert(text, frm):
    src = (EN, EN_S) if frm == "en" else (RU, RU_S)
    dst = (RU, RU_S) if frm == "en" else (EN, EN_S)
    out = []
    for ch in text:
        if ch in src[0]:
            out.append(dst[0][src[0].index(ch)])
        else:
            out.append(dst[1][src[1].index(ch)])
    return "".join(out)


EXTRAS = [
    ("en", "ghbdtn"), ("ru", "привет"), ("en", "jy"), ("ru", "шы"), ("en", "on"), ("ru", "он"),
    ("en", "F"), ("en", "a"), ("en", "t`"), ("en", "ghb2"), ("en", "yt"), ("ru", "афк"),
    ("ru", "ща"), ("ru", "Ща"), ("ru", "ща,"), ("ru", "ру"), ("en", "tls"), ("en", "TLS"),
    ("ru", "ин"), ("en", "nfr"), ("en", "xnj"), ("ru", "пше"), ("ru", "пщ"), ("en", "negb"),
    ("en", "wt"), ("en", "is"), ("en", "e-mail"), ("en", "t-shirt"), ("en", "self-made"),
    ("en", "well-known"), ("en", "x-ray"), ("en", "-elm"), ("en", "nfr-nfrb"),
    ("en", ",elm-kfcrf"), ("en", "zrjcm-nfrb"), ("en", "ghbdtn?"), ("en", "dnf"), ("ru", "ёжик"),
    ("ru", "ежик"), ("en", "Ghbdtn!"), ("ru", "руддщ!"), ("en", "k.,jdm"), ("en", "GHBDTN"),
    ("en", "Ghbdtn"), ("en", "ghbdtn-"), ("en", "nfr-"), ("en", "-nfr"), ("en", "nfr--nfr"),
    ("ru", "так-таки"), ("ru", "будь-ласка"), ("en", "a-b"), ("en", "jy-nfr"), ("en", "ghb"),
    ("en", "jy,"), ("en", "Jy"), ("en", "JY"), ("ru", "Шы"), ("en", "ghbdtn2"), ("en", "gh_bd"),
    ("en", "'nj"), ("en", "lfyys["), ("en", "`krf"), ("ru", "ёлка"), ("en", "`kjxrf"),
    ("en", "hello"), ("ru", "руддщ"), ("en", "world"), ("ru", "цщкдв"), ("en", "don't"),
    ("en", "git"), ("en", "ls"), ("en", "ls]"), ("en", "nb["), ("en", "ytn-ytn"),
    ("en", "rnj-nj"), ("en", "rjt-xnj"), ("en", "rjt-rnj"), ("en", "gj-vjtve"),
]


def main():
    tsv = sys.argv[1]
    seen = set()
    lines = []

    def add(lang, text):
        if (lang, text) not in seen:
            seen.add((lang, text))
            lines.append(f"{lang}\t{text}")

    with open(tsv, encoding="utf-8") as f:
        for line in f:
            fields = line.rstrip("\n").split("\t")
            if fields[2] not in ("en", "ru"):
                continue
            add(fields[2], fields[3])
            add("ru" if fields[2] == "en" else "en", convert(fields[3], fields[2]))
    for lang, text in EXTRAS:
        add(lang, text)
        add("ru" if lang == "en" else "en", convert(text, lang))
    sys.stdout.write("\n".join(lines) + "\n")


main()
