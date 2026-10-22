export function validBefore(a: number): number {
  return a;
}

// invalid bytes: ÿş lone € continuation, truncated â‚, overlong À¯, surrogate í €, too large ô€€, truncated 4-byte ğŸ˜
export function validAfter(b: string): string {
  return b + "Ã©â‚¬ğŸ˜€";
}
