fastify.route({ method: 'GET', url: '/obj' });
fastify.route({ method: "POST", url: "/obj-post", handler });
fastify.route({method:'DELETE',url:`/tight/:id`});
fastify.route({ method: 'put', url: '/lower-method' });
fastify.route({ method: 'HEAD', url: '/head-not-matched' });
fastify.route({ method: 'OPTIONS', url: '/options-not-matched' });
fastify.route({ url: '/url-first', method: 'GET' });
fastify.route({
  method: 'GET',
  url: '/multi-line',
});
fastify.route({ method: 'GET', handler, url: '/after-handler' });
fastify.route({ method : 'PATCH' , url : '/spaced-colon' });
fastify.route({ method: 'GET', url: '' });
fastify.route({ method: 'GET', url: "/mismatch' });
fastify.route({ method: 'GET', url: '/a' }); fastify.route({ method: 'POST', url: '/b' });
app.get('/both', h); fastify.route({ method: 'GET', url: '/both-obj' });
const verbs = { method: 'GET', other: 1, url: '/loose-object' };
const config = { method: 'GET' }; const u = { url: '/separate-objects' };
