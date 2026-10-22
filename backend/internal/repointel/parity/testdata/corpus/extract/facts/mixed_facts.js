const express = require('express');
const app = express();
app.get('/health', (req, res) => res.send('ok'));
app.post('/items', create);
router.delete('/items/:id', remove);
require('node-cron').schedule('0 * * * *', sweep);
jobs.register('sync_all', syncAll);
module.exports = app;
