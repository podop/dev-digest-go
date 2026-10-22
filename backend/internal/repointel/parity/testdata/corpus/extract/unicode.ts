// héllo wörld — comment with non-ASCII before the declarations
export function grüße(name: string = 'Ünïcödé'): string {
  return name;
}

export const 日本語 = (引数: number) => 引数 * 2;

export function emoji(label: string = '😀😀😀'): string {
  return label;
}

export class Café {
  menü(x: string = 'ä'): string {
    return x;
  }
}
const after = (x: number = 1) => x;
function lineSep() { return 'a b c'; }
function nel() { return 'x…y'; }
function last(a = '😀') { callsLater(); }
function callsLater() {}
