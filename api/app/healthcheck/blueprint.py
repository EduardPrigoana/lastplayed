from flask import Blueprint
from app.healthcheck import healthcheck


def create_blueprint():
    blueprint = Blueprint('Health Check Blueprint', __name__)
    blueprint.route('/')(healthcheck.route)
    return blueprint
