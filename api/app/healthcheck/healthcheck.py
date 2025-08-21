import logging

from flask import jsonify

log = logging.getLogger(__name__)


def route():
    log.info("Healthcheck was requested")
    return jsonify({
        "message": "type shit"
    })
