from flask import Blueprint
from app.songs import latest_songs

def create_blueprint():
    bp = Blueprint('Songs Blueprint', __name__)
    bp.route('/<user>/latest-song', methods=['GET'])(latest_songs.route)
    return bp
