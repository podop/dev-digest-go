const a = () => 1; // note still the same line
const b = () => 2;
const s = 'xy';
function after() {
  b();
}
/* block comment */
function last() { after(); }
