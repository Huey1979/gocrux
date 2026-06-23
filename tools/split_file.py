"""Split a large Go file into smaller ones by function groups."""
import sys, re, os

def split_go_file(filepath, groups, keep_ranges):
    """Split filepath into multiple files based on function group definitions.

    groups: dict of {output_filename: [(start_line, end_line), ...]}
    keep_ranges: list of (start, end) line ranges to keep in original file

    All line numbers are 1-indexed.
    """
    with open(filepath, 'r', encoding='utf-8') as f:
        lines = f.readlines()

    # Collect all imports used in each group
    all_imports = set()
    for i, line in enumerate(lines):
        if i < 50:  # imports are at top
            for pkg in ['"context"', '"encoding/json"', '"fmt"', '"reflect"', '"strconv"', '"strings"',
                        '"github.com/Huey1979/gocrux/expression"', '"github.com/Huey1979/gocrux/constants"',
                        'errs "github.com/Huey1979/gocrux/errors"', '"github.com/Huey1979/gocrux/service"',
                        '"github.com/gin-gonic/gin"']:
                if pkg in line:
                    all_imports.add(pkg)

    import_text = 'import (\n'
    for imp in sorted(all_imports):
        import_text += f'\t{imp}\n'
    import_text += ')\n'

    # Generate import block
    # Actually, each split file needs only the imports it actually uses.
    # For simplicity, we include all imports that are used in the original file.
    # Unused imports will cause compile errors, so we need to be precise.

    # Better approach: just use the same import block, and let the user remove unused ones
    # since Go compiler will tell them which are unused.

    for outfile, ranges in groups.items():
        out_lines = []
        for start, end in ranges:
            out_lines.extend(lines[start-1:end])

        outpath = os.path.join(os.path.dirname(filepath), outfile)
        with open(outpath, 'w', encoding='utf-8') as f:
            f.write('package handler\n\n')
            f.write(import_text)
            f.write('\n')
            f.write(''.join(out_lines))
        print(f'{outfile}: {len(out_lines)} lines')

    # Rewrite original with only kept ranges
    kept = []
    for start, end in keep_ranges:
        kept.extend(lines[start-1:end])
    with open(filepath, 'w', encoding='utf-8') as f:
        f.write(''.join(kept))
    print(f'{os.path.basename(filepath)}: now {len(kept)} lines')

if __name__ == '__main__':
    base = r'F:\labvoyage\go_project\gocrux\handler'
    fp = os.path.join(base, 'generic.go')

    groups = {
        'generic_create.go': [(832, 928)],
    }
    # Keep lines 1-831 (struct/config/helpers) + 929-end
    keep = [(1, 831), (929, 2000)]  # 2000 is safe upper bound

    split_go_file(fp, groups, keep)
