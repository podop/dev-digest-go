define(['./amd_define.js', './amd_define_named.js'], function (a, b) {
  return [a, b];
});
require(['./amd_require_array.js'], function (c) {
  return c;
});
define('named', ['./amd_define_named.js'], function (d) { return d; });
